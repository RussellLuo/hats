# hats

[![Go Reference](https://pkg.go.dev/badge/RussellLuo/hats/vulndb.svg)][1]

Communicating with NATS using the HTTP protocol.


## Installation

```bash
go install github.com/RussellLuo/hats@latest
```

## Prerequisites

Start a JetStream enabled NATS server ([docs][2]):

```bash
docker network create nats
docker run --name nats --network nats -p 4222:4222 -d nats -js
```

Create a stream ([docs][3]):

```bash
nats stream add ORDERS --subjects='ORDERS.*'
```


## Quick Start

### Using the default webhook

Run the `hats` server:

```bash
hats -sub_stream='{"name":"ORDERS","consumers":[{"durable_name":"ORDERS_CONS"}]}'
```

Publish a message:

```bash
curl -XPOST 'http://127.0.0.1:8080/pub?subject=ORDERS.new' \
  -H 'Content-Type: application/json' \
  -d '{"order_id": "123"}'
```

The published message will be consumed by the default webhook handler, see logs of the `hats` server.

### Using your own webhook

Run the `hats` server:

```bash
hats \
  -sub_stream='{"name":"ORDERS","consumers":[{"durable_name":"ORDERS_CONS"}]}' \
  -sub_webhook_url='http://127.0.0.1:8081/your-own-webhook' \
  -sub_webhook_key='your-own-key'
```

Publish a message:

```bash
curl -XPOST 'http://127.0.0.1:8080/pub?subject=ORDERS.new' \
  -H 'Content-Type: application/json' \
  -d '{"order_id": "123"}'
```

The published message will be consumed by your own webhook handler, turn to your webhook server to see the results.


## Documentation

Checkout the [Godoc][1].


## License

[MIT](LICENSE)


[1]: https://pkg.go.dev/github.com/RussellLuo/hats
[2]: https://docs.nats.io/running-a-nats-service/nats_docker/jetstream_docker
[3]: https://docs.nats.io/nats-concepts/jetstream/streams
