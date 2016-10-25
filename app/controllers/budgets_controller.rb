class BudgetsController < ApplicationController
  before_action :authenticate_user!

  def index
    # Index should show all budgets for a user
    @budgets = current_user.budget
  end

  def show
		@budget = Budget.find(params[:id])
    @users = @budget.user
  end

  def new
    @budget = Budget.new
  end

  def create
		@budget = Budget.new(budget_params)
    @budget.user<<current_user
		if @budget.save
    	flash[:success] = "Successfully created new budget!"
      redirect_to root_url
		else
			render 'new'
		end
  end

  def edit
		@budget = Budget.find(params[:id])
  end

  def update
		@budget = Budget.find(params[:id])
		if @budget.update_attributes(budget_params)
    	flash[:success] = "Successfully updated budget!"
      redirect_to root_url
		else
			render 'new'
		end
  end

  private
		def budget_params
			params.require(:budget).permit(:name)
		end

end
