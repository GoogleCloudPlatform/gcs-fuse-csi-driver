#!/bin/bash
set -e

usage() {
    cat <<EOF
Generate certificate suitable for use with a webhook service.
This script generates a local CA and a certificate signed by it,
suitable for use with webhook services. 
The server key and cert are stored in a k8s secret.
usage: ${0} [OPTIONS]
The following flags are required.
       --service          Service name of webhook.
       --namespace        Namespace where webhook service and secret reside.
       --secret           Secret name for CA certificate and server certificate/key pair.
EOF
    exit 1
}

while [[ $# -gt 0 ]]; do
    case ${1} in
        --service)
            service="$2"
            shift
            ;;
        --secret)
            secret="$2"
            shift
            ;;
        --namespace)
            namespace="$2"
            shift
            ;;
        *)
            usage
            ;;
    esac
    shift
done

[ -z ${service} ] && service=gcsfuse-csi-memory-webhook-service
[ -z ${secret} ] && secret=gcsfuse-csi-memory-webhook-secret
[ -z ${namespace} ] && namespace=kube-system

if [ ! -x "$(command -v openssl)" ]; then
    echo "openssl not found"
    exit 1
fi

tmpdir=$(mktemp -d)
echo "creating certs in tmpdir ${tmpdir} "

cat <<EOF >> ${tmpdir}/csr.conf
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
[req_distinguished_name]
[ v3_req ]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = ${service}
DNS.2 = ${service}.${namespace}
DNS.3 = ${service}.${namespace}.svc
EOF

# Generate server key and CSR (removed system:node from subject)
openssl genrsa -out ${tmpdir}/server-key.pem 2048
openssl req -new -key ${tmpdir}/server-key.pem -subj "/CN=${service}.${namespace}.svc" -out ${tmpdir}/server.csr -config ${tmpdir}/csr.conf

# Generate a local CA (Certificate Authority)
openssl genrsa -out ${tmpdir}/ca-key.pem 2048
openssl req -x509 -new -nodes -key ${tmpdir}/ca-key.pem -days 3650 -out ${tmpdir}/ca-cert.pem -subj "/CN=Webhook_Local_CA"

# Sign the server CSR with the local CA
openssl x509 -req -in ${tmpdir}/server.csr -CA ${tmpdir}/ca-cert.pem -CAkey ${tmpdir}/ca-key.pem -CAcreateserial -out ${tmpdir}/server-cert.pem -days 3650 -extensions v3_req -extfile ${tmpdir}/csr.conf

# create the secret with server cert/key
kubectl create secret generic ${secret} \
        --from-file=key.pem=${tmpdir}/server-key.pem \
        --from-file=cert.pem=${tmpdir}/server-cert.pem \
        --dry-run=client -o yaml |
    kubectl -n ${namespace} apply -f -

echo ""
echo "===================================================================="
echo "IMPORTANT: You must update your MutatingWebhookConfiguration YAML"
echo "with the following caBundle so the API server trusts this local cert:"
echo "===================================================================="
cat ${tmpdir}/ca-cert.pem | base64 | tr -d '\n'
echo ""
echo "===================================================================="
