package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fortnoxab/fnxlogrus"
	"github.com/jonaz/gograce"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var metricsAddr = flag.String("listen-address", ":8080", "The address to listen on for HTTP metrics requests.")
var logLevel = flag.String("log-level", "info", "loglevel")
var certPath = flag.String("cert-path", "/etc/kubernetes/ssl", "the cert path to look in for etcd client certs")
var certRegexString = flag.String("cert-regex", "kube-etcd.*.pem", "regexp to use when look for etcd certs in cert-path. Will use the fist one found only.")
var secretConfig = flag.String("secret", "monitoring/etcd-cert", "configure in which namespace and secret to copy the certs to")
var namespace string
var secret string

var kubeClient corev1client.CoreV1Interface
var secretsClient corev1client.SecretInterface

var certRegex *regexp.Regexp

func main() {
	flag.Parse()
	fnxlogrus.Init(fnxlogrus.Config{Format: "json", Level: *logLevel}, logrus.StandardLogger())

	tmp := strings.Split(*secretConfig, "/")
	if len(tmp) != 2 {
		logrus.Fatalf("-secret config invalid should contain <namespace>/<secretname>")
	}
	namespace = tmp[0]
	secret = tmp[1]

	kubeClient = getKubeClient()
	secretsClient = kubeClient.Secrets(namespace)

	certRegex = regexp.MustCompile(*certRegexString)

	http.Handle("/metrics", promhttp.Handler())
	srv, shutdown := gograce.NewServerWithTimeout(10 * time.Second)
	srv.Handler = http.DefaultServeMux
	srv.Addr = *metricsAddr

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		err := srv.ListenAndServe()
		if err != nil && errors.Is(err, http.ErrServerClosed) {
			logrus.Error(err)
		}
	}()

	go periodicSyncer(shutdown)
	<-shutdown
	wg.Wait()
}

func periodicSyncer(stopc <-chan struct{}) {
	syncAndLog()
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stopc:
			return
		case <-ticker.C:
			syncAndLog()
		}
	}
}

func syncAndLog() {
	err := syncCertToSecret()
	if err != nil {
		logrus.Error(err)
	}
}

func syncCertToSecret() error {
	logrus.Debugf("starting sync of certs to secret %s/%s", namespace, secret)
	// get cert and key from local disk with configured regexp
	folders, err := ioutil.ReadDir(*certPath)
	if err != nil {
		return err
	}

	var filename string
	for _, file := range folders {
		filename = filepath.Join(*certPath, file.Name())
		if certRegex.MatchString(filename) {
			logrus.Debugf("found file %s to sync", filename)
			break //only sync the first found
		}
	}
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("cound not read file %s: %w", filename, err)
	}

	isPrivateKey := bytes.Contains(content, []byte("PRIVATE KEY"))

	var certFile, keyFile string
	for _, f := range folders {
		secondFile := filepath.Join(*certPath, f.Name())
		if isPrivateKey {
			_, err = tls.LoadX509KeyPair(secondFile, filename)
			if err == nil {
				certFile = secondFile
				keyFile = filename
				break
			}
		} else {
			_, err = tls.LoadX509KeyPair(filename, secondFile)
			if err == nil {
				certFile = filename
				keyFile = secondFile
				break
			}
		}
	}

	if err != nil {
		return err
	}

	logrus.Debugf("found cert %s", certFile)
	logrus.Debugf("found key %s", keyFile)

	return saveSecret(certFile, keyFile)
}

func saveSecret(certFile, keyFile string) error {
	certBytes, err := ioutil.ReadFile(certFile)
	if err != nil {
		return err
	}

	keyBytes, err := ioutil.ReadFile(keyFile)
	if err != nil {
		return err
	}

	secretData := make(map[string][]byte)

	secretData["cert.pem"] = certBytes
	secretData["key.pem"] = keyBytes
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secret,
			Namespace: namespace,
		},
		Data: secretData,
	}

	return CreateOrUpdateSecret(secret)
}

func CreateOrUpdateSecret(secret *v1.Secret) error {
	secrets, err := secretsClient.Get(secret.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("retrieving existing secrets object failed: %w", err)
	}

	if apierrors.IsNotFound(err) {
		_, err = secretsClient.Create(secret)
		if err != nil {
			return fmt.Errorf("creating secrets object failed: %w", err)
		}
		logrus.Debugf("created secret %s successfully in namespace %s", secret.GetName(), namespace)
	} else {
		secret.ResourceVersion = secrets.ResourceVersion
		_, err = secretsClient.Update(secret)
		if err != nil {
			return fmt.Errorf("updating secrets object failed: %w", err)
		}
		logrus.Debugf("updated secret %s successfully in namespace %s", secret.GetName(), namespace)
	}

	return nil
}

func getKubeClient() corev1client.CoreV1Interface {
	var kubeconfig string
	if os.Getenv("KUBECONFIG") != "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	} else if home := homedir.HomeDir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		logrus.Info("No kubeconfig found. Using incluster...")
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logrus.Error("error kubernetes.NewForConfig")
		panic(err)
	}
	return clientset.CoreV1()
}
