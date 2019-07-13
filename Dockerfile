# MyPersonalBudget Dockerfile
# VERSION 2.0

################################################################################
# Builder
################################################################################
FROM ruby:2.6-alpine as builder
RUN apk add --no-cache --update \
        build-base \
        nodejs \
        libpq \
        postgresql \
        libxml2-dev \
        postgresql-dev \
        postgresql-client

WORKDIR /src
COPY Gemfile* ./
RUN gem install bundler:2.0.1 \
  && bundle install -j4 --retry 3 \
  && rm -rf /usr/local/bundle/cache/*.gem \
  && find /usr/local/bundle/gems/ -name "*.c" -delete \
  && find /usr/local/bundle/gems/ -name "*.o" -delete

ADD . ./
RUN mkdir -p ./tmp/cache ./log

################################################################################
# Production
################################################################################
FROM ruby:2.6-alpine as prod

RUN apk add --no-cache --update \
        nodejs \
        postgresql-client \
        tzdata \
  && addgroup -g 1000 -S app \
  && adduser -u 1000 -S app -G app
    
WORKDIR /usr/src/mpb
COPY --from=builder /usr/local/bundle/ /usr/local/bundle/
COPY --from=builder --chown=app:app /src ./

RUN echo -e '#!/bin/sh\ncd /usr/src/mpb\nrake run_payroll' > /etc/periodic/hourly/mpb \
  && chmod 777 /etc/periodic/hourly/mpb

HEALTHCHECK --interval=30s --timeout=3s \
  CMD echo -e 'require "net/http"\nNet::HTTP.get(URI("http://127.0.0.1:3000/"))' | ruby

USER app
CMD [ "/bin/sh", "/usr/src/mpb/docker-entrypoint.sh" ]
EXPOSE 3000
