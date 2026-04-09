# Endorser client (example implmentation)

The endorser client is a minimal developer debugging CLI that shows how to use the SDK's
`EndorsementClient` and `FabricSubmitter` to drive a full transaction end-to-end. It is
the client-side counterpart to the endorser service: propose a transaction, collect
endorsements, and submit to the orderer.

Configuration is a single YAML file that lists the endorser and orderer endpoints along
with the MSP identity used for signing. Sample configs are in the `config/` folder of
the endorser example:

| Config file               | Network  |
| ------------------------- | -------- |
| `config/fabx-client.yaml` | Fabric-X |
| `config/fab3-client.yaml` | Fabric 3 |

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
