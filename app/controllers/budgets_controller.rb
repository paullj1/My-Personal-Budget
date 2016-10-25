class BudgetsController < ApplicationController
  before_action :authenticate_user!

  def show
		@budget = Budget.find(params[:id])
    @authorized_users = User.where(id: @budet.authorized_users.to_a).select(:id, :email)
  end

  def new
    @budget = Budget.new
  end

  def create
		@budget = Budget.new(budget_params)
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
		if @budget.update_attributes(problem_params)
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
