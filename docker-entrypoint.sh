#!/bin/bash
crond
bundle exec puma -C "/usr/src/mpb/config/puma.rb" -b "tcp://0.0.0.0:3000"
