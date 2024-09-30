#!/bin/bash

kubectl delete -f k8s/metal3-fkas-system-deployment.yaml

docker build -t quay.io/metal3-io/metal3-fkas:latest .

minikube image rm quay.io/metal3-io/metal3-fkas:latest

minikube image load quay.io/metal3-io/metal3-fkas:latest

kubectl apply -f k8s/metal3-fkas-system-deployment.yaml
