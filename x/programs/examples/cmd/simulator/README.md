### Program Simulator

## Introduction

Program Simulator

#### build

```sh
go build cmd/simulator/simulator.go
```

### generate new private key and return address

```sh
./simulator key generate
created new private key with public address: simulator1fhfjlv9cu0psq276d3ve0nr0yl4daxvud2kcp3w8t3txcwk9t2esrfd0r7
```

### create new program tx

```sh
./simulator \
  program create \
  --caller simulator1fhfjlv9cu0psq276d3ve0nr0yl4daxvud2kcp3w8t3txcwk9t2esrfd0r7 \
  ./path/to/program.wasm
created program tx successful: 1
```

### invoke program tx

```sh
./simulator \
  program invoke \
  --caller simulator1fhfjlv9cu0psq276d3ve0nr0yl4daxvud2kcp3w8t3txcwk9t2esrfd0r7
  --id 1 \
  --function "set" \
  --params "1" \
  --max-fee 30

created invoke tx successful: 2mcwQKiD8VEspmMJpL1dc7okQQ5dDVAWeCBZ7FWBFAbxpv3t7w
response: 0
```