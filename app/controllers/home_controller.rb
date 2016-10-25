class HomeController < ApplicationController
  before_action :authenticate_user!

  def index
    @budgets = current_user.budget
  end

end
