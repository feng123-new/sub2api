# syntax=docker/dockerfile:1.7
FROM rust:1.92-alpine AS builder
RUN apk add --no-cache musl-dev
ENV CARGO_BUILD_JOBS=2
WORKDIR /src
COPY codex-transport-rs/Cargo.toml codex-transport-rs/Cargo.lock ./
COPY codex-transport-rs/src ./src
RUN cargo build --locked --release
RUN cargo test --locked --release

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /src/target/release/sub2api-codex-transport /usr/local/bin/sub2api-codex-transport
ENV CODEX_RUST_SOCKET=/run/codex-rust/transport.sock
ENTRYPOINT ["/usr/local/bin/sub2api-codex-transport"]
