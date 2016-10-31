class HomeController < ApplicationController

  def index
    @budgets = current_user.budget
  end

end
