FROM dhi.io/golang:1.27-dev AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-s -w" -o /frontend ./cmd/frontend

FROM scratch
COPY --from=build /frontend /bin/frontend
ENTRYPOINT ["/bin/frontend"]
