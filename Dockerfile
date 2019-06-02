# MyPersonalBudget Dockerfile
# VERSION 1.0

FROM ruby:2.5-alpine
MAINTAINER Paul Jordan <paullj1@gmail.com>

HEALTHCHECK --interval=30s --timeout=3s \
  CMD curl -f http://127.0.0.1:3000/ || exit 1

RUN apk update && apk add \
        build-base \
        nodejs \
        libpq \
        curl \
        curl-dev \
        postgresql \
        libxml2-dev \
        postgresql-dev \
        postgresql-client

RUN mkdir /usr/src/mpb
WORKDIR /usr/src/mpb
ADD Gemfile Gemfile.lock /usr/src/mpb/
RUN gem install bundler && bundle install 
ADD . /usr/src/mpb/

RUN echo -e '#!/bin/sh\ncd /usr/src/mpb\nrake run_payroll' > /etc/periodic/hourly/mpb \
  && chmod 777 /etc/periodic/hourly/mpb

CMD [ "/bin/sh", "/usr/src/mpb/docker-entrypoint.sh" ]
EXPOSE 3000
