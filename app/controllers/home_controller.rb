class HomeController < ApplicationController

  def index
    @budgets = current_user.budget.order(:id)
  end

end
