class HomeController < ApplicationController

  def index
    @budgets = current_user.budget.order(:id)
    respond_to do |format|
      format.html # index.html.erb
      format.json { render json: @budgets, status: :ok }
    end
  end

end
