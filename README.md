<!--
SPDX-License-Identifier: Apache-2.0
-->

# Fabric-X Client SDK

This SDK provides a modular set of building blocks that can be used to develop client applications, endorsers, and custom components for Fabric-X and Hyperledger Fabric 3.

## Status

This preview version can be used for testing and prototypes. It does not provide the level of resilience you would need for production yet, and APIs will change without warning.

## Prerequisites

| Tool   | Version | Install                              |
| ------ | ------- | ------------------------------------ |
| Go     | 1.26+   | <https://go.dev/dl>                  |
| Docker | 20.x+   | <https://docs.docker.com/get-docker> |

## Getting started

```sh
go get github.com/hyperledger/fabric-x-sdk
```

Then import whichever packages you need:

```go
import (
    "github.com/hyperledger/fabric-x-sdk/network"
    "github.com/hyperledger/fabric-x-sdk/blocks"
    "github.com/hyperledger/fabric-x-sdk/identity"
)
```

Run the unit tests:

```sh
go test ./... -short
```

To run the integration tests against a real Fabric-X network, see the [integration](integration/) package.

## Components

| Package         | Description                                                  |
| --------------- | ------------------------------------------------------------ |
| **blocks**      | Tools to parse Fabric or Fabric-X blocks. Also includes a simple Processor that can execute pluggable handlers. |
| **endorsement** | Reads a Fabric-style SignedProposal (the request made to a peer to execute chaincode). Can also create a signed response, with a transaction or read/write set style that can be either Fabric- or Fabric-X format. Fabric-X does not support "traditional" chaincode, but this package makes it possible to create signed endorsements of read/write sets following the new programming model. |
| **fabrictest**  | Mimics a minimal in-memory Fabric or Fabric-X network for tests. Implements the orderer Broadcast API and the peer Deliver endpoint. |
| **identity**    | Has the bare minimum for Fabric ecdsa identities. |
| **integration** | Provides a couple of tests that exercise a large surface of the SDK. It provides some assurance on the internal consistency of the project. By default, the internal `fabrictest` fake network is used. It can also be pointed to real networks.|
| **local**       | Can be used to mock a Fabric backend for testing. Instead of submitting to an orderer, it can insert read/write sets directly in a local database. Some MVCC checks are provided, but should not be trusted to be 1:1 compatible with a real network. |
| **network**     | Has clients for peers and orderers, along with simple wrappers to submit a transaction and to synchronize a local database with a committer. |
| **notification** | Subscribes to transaction events from the Fabric-X sidecar. `Notifier` tracks specific transactions you submitted; `AllTxStreamer` delivers a real-time feed of every committed transaction (optionally filtered by namespace or status). |
| **state**       | Provides a versioned insert-only database that can be used to construct a local world state. For example, an endorser could use it to perform actions on a recent state in order to generate a read/write set based on custom business logic. |

## Receiving transaction events

There are three complementary mechanisms for receiving committed transaction data, each suited to a different need:

| Mechanism | When to use |
| --------- | ----------- |
| `network.Synchronizer` | Maintain a local world state or full block history. Supports catch-up from any block height, reconnection, and liveness/readiness probes. Requires a signer. |
| `notification.Notifier` | Know when a transaction *you* submitted commits or fails. Opens a bidirectional stream; you push txIDs to watch and receive back their status. |
| `notification.AllTxStreamer` | React to *all* committed transactions (optionally filtered by namespace or status). Real-time feed only — no historical replay. Useful for audit logs, analytics, secondary indexes, or watching activity from other participants. |

All three can be used independently and composed in the same application. Both `Notifier` and `AllTxStreamer` are backed by the Fabric-X sidecar's Notification Service and require no signer.

---

For example, if you were building a block explorer, you would take the `network.Synchronizer`, load it with an `identity.Signer` and point to the committer sidecar. With help of the `blocks.Processor`, the `fabric(x).Parser` and a custom handler, you extract the information you want to store from the blocks that are coming in through the Synchronizer. Then you build your application around it.

If you were building a chaincode endorser, you would use the same elements, as well as the
`endorsement.ProposalBuilder`, world state storage like the provided in `state.VersionedDB` with a
`state.SimulationStore` on top. You might choose to expose the classic Fabric ProcessProposal API.
To complete the picture, you could equip your client applications with an endorsement client from
`network.Peer` and the `network.Submitter`.

For an end-to-end example endorser application, check out
[custom-endorser](https://github.com/hyperledger/fabric-x-samples/tree/main/custom-endorser) in the
fabric-x-samples repo. It leans heavily on the code of this SDK.

## Useful links

- [Fabric-X](https://github.com/hyperledger/fabric-x) — architecture overview and motivation
- [Fabric-X Samples](https://github.com/hyperledger/fabric-x-samples) — end-to-end example applications
- [Fabric-X Committer](https://github.com/hyperledger/fabric-x-committer) — post-ordering transaction processing
- [Fabric-X Orderer](https://github.com/hyperledger/fabric-x-orderer) — BFT ordering service (Arma protocol)
- [Fabric-X Common](https://github.com/hyperledger/fabric-x-common) — shared protobuf and CLI tooling
- [Fabric-X Ansible Collection](https://github.com/LF-Decentralized-Trust-labs/fabric-x-ansible-collection) — Ansible scripts for deploying Fabric-X

## Contributing

Contributions are welcome. Please read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) before opening a pull request.

## License

Apache License 2.0 — see [LICENSE](LICENSE).