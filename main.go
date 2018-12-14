package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/golang/glog"
	"github.com/spf13/pflag"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	var parameters WhSvrParameters

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	flag.CommandLine.Parse([]string{})
	pflag.Lookup("logtostderr").Value.Set("true")

	// get command line parameters
	pflag.IntVar(&parameters.port, "port", 443, "Webhook server port.")
	pflag.StringVar(&parameters.certFile, "tls-cert-file", "/etc/webhook/certs/cert.pem",
		"File containing the default x509 Certificate for HTTPS. (CA cert, if any, concatenated "+
			"after server cert).")
	pflag.StringVar(&parameters.keyFile, "tls-private-key-file", "/etc/webhook/certs/key.pem",
		"File containing the default x509 private key matching --tls-cert-file.")
	pflag.StringSliceVar(&parameters.ignoredNamespaces, "ignore-namespaces", []string{metav1.NamespaceSystem, metav1.NamespacePublic}, "Namespaces to ingore")
	pflag.StringVar(&parameters.tz, "tz", "Asia/Shanghai", "Which timezone file to mount into")
	pflag.Parse()

	tz := tzFile{
		name:      "local-tz",
		hostPath:  fmt.Sprintf("/usr/share/zoneinfo/%s", parameters.tz),
		mountPath: "/etc/localtime",
	}

	// Test if the hostpath of timezone file is exists
	_, err := os.Stat(tz.hostPath)
	if err != nil {
		glog.Fatalf("Failed to find timezone file: %v", err)
	}

	pair, err := tls.LoadX509KeyPair(parameters.certFile, parameters.keyFile)
	if err != nil {
		glog.Fatalf("Filed to load key pair: %v", err)
	}

	whsvr := &WebhookServer{
		ignoredNamespaces: parameters.ignoredNamespaces,
		tz:                tz,
		server: &http.Server{
			Addr:      fmt.Sprintf(":%v", parameters.port),
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}},
		},
	}

	// define http server and server handler
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", whsvr.serve)
	whsvr.server.Handler = mux

	// start webhook server in new rountine
	go func() {
		glog.Infof("Webhook server is listening on port %d", parameters.port)
		if err := whsvr.server.ListenAndServeTLS("", ""); err != nil {
			glog.Errorf("Filed to listen and serve webhook server: %v", err)
		}
	}()

	// listening OS shutdown singal
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	glog.Infof("Got OS shutdown signal, shutting down webhook server gracefully...")
	whsvr.server.Shutdown(context.Background())
}
