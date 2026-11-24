FROM golang:1.26.11-trixie@sha256:fa340cb59ebeb1d53ac1cccfc334201d938fe5f3458003cccb767096d8e032b8 AS builder
WORKDIR /fixture
COPY main.go ./
ARG VARIANT=a
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.variant=${VARIANT}" -o /fixture/server main.go
FROM gcr.io/distroless/static-debian13@sha256:15f8637509792e05b97d83451b0b6ccfa4abd548f997e406ac1e72c87fe1fa78
COPY --from=builder /fixture/server /server
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/server"]
