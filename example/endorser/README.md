# Endorser - Example Fabric-X Service

This example demonstrates how the `fabric-x-sdk` can be used together with components of `fabric-x-common` and the `fabric-x-committer` to create a new Fabric or Fabric-X service.

The Endorser service is analogous to endorsing peers running chaincode in Fabric, with the difference that the service does not commit blocks itself; it merely synchronizes its world state with a committing peer. The endorser is responsible for a single namespace. The endorsement policy determines how many (and which) endorser have to sign the update to the ledger for it to be committed. In classic Fabric that means a chaincode -- any chaincode -- must be installed with the name of the namespace. In Fabric-X, the namespace can be created with `fxconfig`. Outside of that, the endorsers are free in how they construct their read/write sets.

Features:

- Configure as a component of a Fabric or Fabric-X network
- Fabric ProcessProposal API (like for chaincode execution)
- Synchronizes world state from a committing peer
- Generates and endorses (signs) read/write sets
- Mutual TLS authentication
- Client cli tool that contacts the endorsers and then the orderer to create a transaction.

## Quick Start

### Run the Service: Fabric-X

From the repo root, run a fabric-x test committer and a single endorser:

```shell
# start a fabric-x test network
make start-x

# start the example endorser services (in two different consoles)
cd example/endorser/
go run cmd/endorser/main.go -c sampleconfig/fabx-endorser1.yaml
go run cmd/endorser/main.go -c sampleconfig/fabx-endorser2.yaml
```

#### Test with the client CLI

The `cmd/client` dev tool drives a full transaction: proposal -> endorsement -> submission.
See [cmd/client/README.md](cmd/client/README.md) for details.

In another terminal, execute the following commands to invoke a simple transaction and query the ledger. 

```shell
# invoke: endorse and submit to the orderer
go run cmd/client/main.go -c sampleconfig/fabx-client.yaml invoke '{"Args":["set", "greeting", "hello world"]}'

# query: endorse only, print the response payload
go run cmd/client/main.go -c sampleconfig/fabx-client.yaml query '{"Args":["get", "greeting"]}'
```

Note that `invoke` does not wait for finality; it returns after submitting to the orderer.

### Run the Service: classic Fabric

> [!NOTE]
> Instructions to run Fabric will be added later. The following instructions
> assume a fabric-samples network running in `../../testdata/fabric-samples`,
> similar to how it's started in [fabric-x-evm](https://github.com/hyperledger/fabric-x-evm).

```shell
go run cmd/endorser/main.go -c sampleconfig/fab-endorser1.yaml
go run cmd/endorser/main.go -c sampleconfig/fab-endorser2.yaml
```

#### Test on classic Fabric

For this example, we're using the `peer` CLI. This CLI only works if we use classic Fabric as a backend; it submits transactions in a format that Fabric-X cannot parse. Use the client CLI above for Fabric-X.

##### Invoke (set greeting)

```shell
SAMPLES="$(realpath ../../testdata/fabric-samples)"
ORG1=$SAMPLES/test-network/organizations/peerOrganizations/org1.example.com
ORG2=$SAMPLES/test-network/organizations/peerOrganizations/org2.example.com
ORDERER=$SAMPLES/test-network/organizations/ordererOrganizations/example.com

FABRIC_CFG_PATH=$SAMPLES/config \
GRPC_ENFORCE_ALPN_ENABLED=false \
CORE_PEER_LOCALMSPID=Org1MSP \
CORE_PEER_MSPCONFIGPATH=$ORG1/users/Admin@org1.example.com/msp \
CORE_PEER_TLS_ENABLED=true \
CORE_PEER_TLS_CLIENTAUTHREQUIRED=true \
CORE_PEER_TLS_CLIENTCERT_FILE=$ORG1/users/Admin@org1.example.com/tls/client.crt \
CORE_PEER_TLS_CLIENTKEY_FILE=$ORG1/users/Admin@org1.example.com/tls/client.key \
CORE_PEER_ADDRESS=localhost:9001 \
peer chaincode invoke \
  -o localhost:7050 \
  --ordererTLSHostnameOverride orderer.example.com \
  --tls \
  --cafile $ORDERER/orderers/orderer.example.com/tls/ca.crt \
  -C mychannel \
  -n basic \
  --peerAddresses localhost:9001 \
  --tlsRootCertFiles $ORG1/peers/peer0.org1.example.com/tls/ca.crt \
  --peerAddresses localhost:9002 \
  --tlsRootCertFiles $ORG2/peers/peer0.org2.example.com/tls/ca.crt \
  -c '{"function":"set","Args":["greeting", "hello world"]}'
```

##### Query (get greeting)

```shell
SAMPLES="$(realpath ../../testdata/fabric-samples)"
ORG1=$SAMPLES/test-network/organizations/peerOrganizations/org1.example.com

FABRIC_CFG_PATH=$SAMPLES/config \
GRPC_ENFORCE_ALPN_ENABLED=false \
CORE_PEER_LOCALMSPID=Org1MSP \
CORE_PEER_MSPCONFIGPATH=$ORG1/users/Admin@org1.example.com/msp \
CORE_PEER_TLS_ENABLED=true \
CORE_PEER_TLS_CLIENTAUTHREQUIRED=true \
CORE_PEER_TLS_CLIENTCERT_FILE=$ORG1/users/Admin@org1.example.com/tls/client.crt \
CORE_PEER_TLS_CLIENTKEY_FILE=$ORG1/users/Admin@org1.example.com/tls/client.key \
CORE_PEER_ADDRESS=localhost:9001 \
peer chaincode query \
  -C mychannel \
  -n basic \
  -c '{"function":"get","Args":["greeting"]}'
```

## Configuration

### Configuration Files

See the `config` folder.

### TLS Modes

The service supports three TLS modes:

1. **`none`** - No TLS (for local development)
2. **`tls`** - One-sided TLS (server certificate only)
3. **`mtls`** - Mutual TLS (both server and client certificates)

### Environment Variables

All configuration can be overridden via environment variables with the `ENDORSER_` prefix:

```shell
export ENDORSER_SERVER_ENDPOINT_PORT=8080 go run cmd/endorser/main.go -c sampleconfig/fab-endorser1.yaml
```

## Project Structure

```
├── cmd/
│   ├── endorser/        # Endorser service entry point
│   └── client/          # Developer client CLI entry point
├── internal/
│   ├── sampleconfig/          # Service configuration structures
│   └── service/         # Service implementation and integration tests
├── sampleconfig/              # Sample configuration files
├── go.mod
└── README.md
```

## Core dependencies

### Fabric-X SDK

This example uses the **fabric-x-sdk** for Fabric integration:

- `fabric-x-sdk` - Reusable SDK for Fabric and Fabric-X applications
  - `blocks` - Block parsing and processing
  - `endorsement` - Fabric and Fabric-X endorsement building
  - `identity` - MSP identity and signing
  - `network` - Peer synchronization and block delivery
  - `state` - Versioned state database

### Fabric-X Committer

- `utils/connection` - GRPC server setup

# Client CLI (example implementation)

The endorser client is a minimal developer debugging CLI that shows how to use the SDK's
`EndorsementClient` and `FabricSubmitter` to drive a full transaction end-to-end. It is
the client-side counterpart to the endorser service: propose a transaction, collect
endorsements, and submit to the orderer.

Configuration is a single YAML file that lists the endorser and orderer endpoints along
with the MSP identity used for signing. Sample configs are in the `config/` folder of
the endorser example:

| Config file               | Network        |
| ------------------------- | -------------- |
| `config/fabx-client.yaml` | Fabric-X       |
| `config/fab-client.yaml`  | Classic Fabric |

```shell
# write a value (endorse + submit)
go run ./cmd/client invoke -c config/fabx-client.yaml '{"function":"set","Args":["greeting","hello world"]}'

# read a value (endorse only, prints payload)
go run ./cmd/client query -c config/fabx-client.yaml '{"function":"get","Args":["greeting"]}'
```

The transaction argument follows the Fabric peer CLI convention: a JSON object with a
`function` key and an `Args` array, or just an `Args` array with the function as first argument.

`query` sends a proposal to the endorsers and prints the response payload. It does not
submit to the orderer.

`invoke` sends a proposal to the endorsers and submits the resulting transaction to the
orderer. It does not wait for finality.
