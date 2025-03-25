package cloud_hypervisor

//go:generate bash -c "mkdir -p client && curl -s https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/master/vmm/src/api/openapi/cloud-hypervisor.yaml | ../bin/oapi-codegen -package=client -generate=types,client,spec -o=./client/client.go /dev/stdin"
