# MyPersonalBudget Dockerfile
################################################################################
# Builder
################################################################################
FROM ruby:3.1-alpine3.16 as builder
RUN apk add --no-cache \
        alpine-sdk \
        postgresql-dev \
        xz

WORKDIR /src
COPY Gemfile* ./
RUN gem install bundler:2.1.2 \
  && gem install nokogiri --platform=ruby \
  && bundle install -j4 --retry 3 \
  && rm -rf /usr/local/bundle/cache/*.gem \
  && find /usr/local/bundle/gems/ -name "*.c" -delete \
  && find /usr/local/bundle/gems/ -name "*.o" -delete

ADD . ./
RUN mkdir -p ./tmp/cache ./log

################################################################################
# Production
################################################################################
FROM ruby:3.1-alpine3.16 as prod

RUN apk add --no-cache \
        nodejs \
        postgresql-client \
        sudo \
        tzdata \
        xz-libs \
  && addgroup -g 1000 -S app \
  && adduser -u 1000 -S app -G app
    
WORKDIR /usr/src/mpb
COPY --from=builder /usr/local/bundle/ /usr/local/bundle/
COPY --from=builder --chown=app:app /src ./

RUN echo -e '#!/bin/sh\ncd /usr/src/mpb\nbundle exec rake run_payroll' > /etc/periodic/hourly/mpb \
  && chmod 777 /etc/periodic/hourly/mpb

HEALTHCHECK --interval=30s --timeout=3s \
  CMD echo -e 'require "net/http"\nexit(Net::HTTP.get_response(URI("http://127.0.0.1:3000/")).code.to_i < 400)' | ruby

RUN echo 'app ALL=NOPASSWD: /usr/sbin/crond' >> /etc/sudoers \
 && mkdir -m 777 -p tmp/pids \
 && ln -s /lib/ld-musl-aarch64.so.1 /lib/ld-linux-aarch64.so.1
USER app
CMD [ "/bin/sh", "/usr/src/mpb/docker-entrypoint.sh" ]
EXPOSE 3000
