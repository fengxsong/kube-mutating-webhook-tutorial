# Kubernetes Admission Webhook example

This example shows how to build and deploy a [MutatingAdmissionWebhook](https://kubernetes.io/docs/admin/admission-controllers/#mutatingadmissionwebhook-beta-in-19) that used for automatic injection of timezone file into pod.

## Prerequisites

Kubernetes 1.9.0 or above with the `admissionregistration.k8s.io/v1beta1` API enabled. Verify that by the following command:
```
kubectl api-versions | grep admissionregistration.k8s.io/v1beta1
```
The result should be:
```
admissionregistration.k8s.io/v1beta1
```

In addition, the `MutatingAdmissionWebhook` and `ValidatingAdmissionWebhook` admission controllers should be added and listed in the correct order in the admission-control flag of kube-apiserver.

## Build

1. Setup dep

   The repo uses [dep](https://github.com/golang/dep) as the dependency management tool for its Go codebase. Install `dep` by the following command:
```
go get -u github.com/golang/dep/cmd/dep
```

2. Build and push docker image(edit `Makefile` first :) )
 
```
make dep
make docker
```

## Deploy

Please read the `Makefile` for more usages.