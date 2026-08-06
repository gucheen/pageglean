FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags sqlite_fts5 -trimpath -ldflags="-s -w" -o /out/links ./cmd/links

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/links /app/links

ENV LINKS_ADDR=:8080
ENV LINKS_DATA_DIR=/data

VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["/app/links"]
CMD ["serve"]
