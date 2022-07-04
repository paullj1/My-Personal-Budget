class ApplicationController < ActionController::Base
  before_action :authenticate_user!
  protect_from_forgery unless: -> { request.format.json? }

  protected

    def json_request?
      request.format.json?
    end
end
