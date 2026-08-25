FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pageglean ./cmd/pageglean

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 pageglean \
    && useradd --uid 10001 --gid 10001 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin pageglean \
    && install -d -o pageglean -g pageglean -m 0750 /data

WORKDIR /app
COPY --from=build /out/pageglean /app/pageglean

ENV PAGEGLEAN_ADDR=:8080
ENV PAGEGLEAN_DATA_DIR=/data

VOLUME ["/data"]
EXPOSE 8080

USER 10001:10001

ENTRYPOINT ["/app/pageglean"]
CMD ["serve"]
