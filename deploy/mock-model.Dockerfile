FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/mock-model ./cmd/mock-model
COPY internal/mockmodel ./internal/mockmodel
RUN go build -o /out/mock-model ./cmd/mock-model

FROM alpine:3.20
COPY --from=build /out/mock-model /usr/local/bin/mock-model
ENTRYPOINT ["mock-model"]
