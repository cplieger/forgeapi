# Non-goals

This page lists what forgeapi leaves out on purpose, with the reason for each. Read it before you ask for one of these, or when you decide whether the library fits your tool.

## A generic request method

It would make every route the caller's business, which is the coupling this library exists to remove. No published price or capability could describe the call. Each operation has one method, with no per-product variant and no option that changes which route it takes.

## Routes you add to a client

The same reason applies. A route added from outside also cannot appear in the generated per-product tables, so its answer would carry facts nothing states.

## A vendor SDK behind any client

Each SDK's types would reach this library's signatures through the answers they return. That would put four other compatibility promises on this library's own. For the same reason, outside its tests forgeapi imports the six modules it depends on only from packages under `internal/`, so none of their types appears in an exported signature.

## Receiving webhooks

An inbound HTTP endpoint belongs to your server, not to a client. Nothing in this library listens.

## Git operations

Clone, fetch and push speak a different protocol to a different service. This library reads and writes the forge's own records.

## A "partly supported" status in the support matrix

A difference between products is per field, and it is stated on the field it affects. A matrix cell meaning "supported with a note" would state the same fact without being able to name which field it is about.

## Reading a proxy from the environment

The person connecting states a proxy on each connection. A proxy read from the environment would apply to every connection at once, and nothing in the connection would show where its traffic goes.

## A polling loop in the API

A polling cycle is one consumer's control flow. Putting it here would publish one tool's loop in an API that another tool could neither use nor remove.

## Code scanning

forgeapi has no code-scanning operation. Adding one would create a product-specific API instead of an operation with one contract across products.

## The OAuth authorization-code grant

It needs a redirect route, which belongs to your own server. A tool that runs it implements `CredentialSource` itself. The device grant that `creds` runs needs no route.
