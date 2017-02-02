package api

//go:generate protoc -I.:../../../vendor:../../../vendor/github.com/gogo/protobuf:../../../../../../..:/usr/local/include:/usr/include --gogoctrd_out=plugins=grpc,import_path=github.com/docker/docker/builder/dockerfile/api,Mgogoproto/gogo.proto=github.com/gogo/protobuf/gogoproto,Mgoogle/protobuf/descriptor.proto=github.com/gogo/protobuf/protoc-gen-gogo/descriptor:. dockerfile.proto
