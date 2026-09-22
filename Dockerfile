# The binary is cross-compiled on the build machine for the target, so
# only the small runtime stage runs under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
	go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /alborz ./cmd/alborz

FROM alpine:3
# Calendars name their zones; Alpine ships none.
RUN apk add --no-cache tzdata \
	&& adduser -D -H -u 10001 alborz \
	&& mkdir /data /cache \
	&& chown alborz /data /cache
COPY --from=build /alborz /usr/local/bin/alborz
USER alborz
EXPOSE 1323
# Without a volume on /data and LBRZ_LOGIN_KEY the container still
# starts; it forgets its sign-ins on a restart and keeps its data inside.
ENTRYPOINT ["alborz", "-data-dir", "/data", "-cache-dir", "/cache"]
