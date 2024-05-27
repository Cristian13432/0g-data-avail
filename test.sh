#!/bin/bash

# Set up cleanup function to stop localstack and exit gracefully
cleanup() {
    docker stop localstack-test
    exit $?
}
trap cleanup EXIT

# Run the deploy command for localstack
go run ./inabox/deploy/cmd -localstack-port=4570 -deploy-resources=false localstack

# Clean test cache before running tests
go clean -testcache

# Set environment variables and run tests
export LOCALSTACK_PORT=4570
export DEPLOY_LOCALSTACK=false
go test -short ./... "$@"
