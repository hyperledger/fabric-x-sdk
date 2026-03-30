# Fabric-X Client SDK

This SDK provides a modular set of building blocks that can be used to develop client applications, endorsers, and custom components for Fabric-X. Some components are designed to be compatible with classic Fabric as well for easy reuse.

## Status

This preview version can be used for testing and prototypes. It does not provide the level of resilience you would need for production yet, and APIs will change without warning.

## Components

- package **blocks** has the tools to parse Fabric or Fabric-X blocks. It also includes a simple Processor that can execute pluggable handlers.
- package **endorsement** can read a Fabric-style SignedProposal (the request made to a peer to execute chaincode). It can also create a signed response, with a transaction
 or read/write set style that can be either Fabric- or Fabric-X format. Fabric-X does not support "traditional" chaincode, but this package makes it possible to create signed endorsements of read/write sets following the new programming model.
- package **fabrictest** mimics a minimal in-memory Fabric or Fabric-X network for tests. It implements the orderer Broadcast API and the peer Deliver endpoint.
- package **identity** has the bare minimum for Fabric ecdsa identities.
- package **integration** provides a couple of tests that exercise a large surface of the SDK. It provides some assurance on the internal consistency of the project. By default, the internal `fabrictest` fake network is used. It can also be pointed to real networks.
- package local can be used to mock a Fabric backend for testing. Instead of submitting to an orderer, it can insert read/write sets directly in a local database. Some MVCC checks are provided, but should not be trusted to be 1:1 compatible with a real network.
- package **network** has clients for peers and orderers, along with simple wrappers to submit a transaction and to synchronize a local database with a committer.
- package **state** provides a versioned insert-only database that can be used to construct a local world state. For example, an endorser could use it to perform actions on a recent state in order to generate a read/write set based on custom business logic.

For example, if you were building a block explorer, you would take the `network.Synchronizer`, load it with an `identity.Signer` and point to the committer sidecar. With help of the `blocks.Processor`, the `fabric(x).Parser` and a custom handler, you extract the information you want to store from the blocks that are coming in through the Synchronizer. Then you build your application around it.

If you were building a chaincode endorser, you would use the same elements, as well as the `endorsement.ProposalBuilder`, world state storage like the provided `state.VersionedDB` with a `state.SimulationStore` on top. You might choose to expose the classic Fabric ProcessProposal API. To complete the picture, you could equip your client applications with an endorsement client from `network.Peer` and the `network.Submitter`.
