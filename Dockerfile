FROM golang:1.23-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/eventrelay ./cmd/eventrelay
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/worker ./cmd/worker

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/eventrelay /usr/local/bin/eventrelay
COPY --from=build /out/worker /usr/local/bin/worker
ENTRYPOINT ["/usr/local/bin/eventrelay"]

FROM runtime AS api
ENTRYPOINT ["/usr/local/bin/eventrelay"]

FROM runtime AS worker
ENTRYPOINT ["/usr/local/bin/worker"]
