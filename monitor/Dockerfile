ARG DIST="/opt"

FROM golang:alpine AS build

ARG DIST

WORKDIR ${DIST}

RUN apk add git

ADD go.* ./
RUN go mod download

ADD  *.go ./
RUN go build


FROM alpine

ARG DIST
ARG DATASET_ID="cromwell_monitoring"

RUN apk add --no-cache ca-certificates

ENV DATASET_ID=${DATASET_ID}

COPY --from=build ${DIST}/monitor /usr/local/bin

USER nobody

ENTRYPOINT ["monitor"]
