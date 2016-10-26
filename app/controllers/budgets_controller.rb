class BudgetsController < ApplicationController
  before_action :authenticate_user!
  before_action :permission?, only: [:show, :edit, :update, :destroy, :share, :unshare]

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
      redirect_to budgets_path
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
      redirect_to budgets_path
		else
			render 'new'
		end
  end

  def destroy
    budget = Budget.find(params[:id])
		budget.destroy
    flash[:success] = "Budget deleted!"
    redirect_to budgets_path
  end

  def unshare
    @budget = Budget.find(params[:id])
    @user = User.find(params[:user_id])

    unless @user
      flash[:danger] = "Invalid user id."
      redirect_to budgets_path
    end

    if @user.id == current_user.id
      flash[:warning] = "Can't remove yourself!"
      redirect_to budgets_path
    end

    @budget.user.delete(@user)
    if @budget.save
      flash[:success] = "Successfully revoked budget permissions from #{@user.email}!"
    else
      flash[:danger] = "Couldn't revoke budget permission from #{@user.email}."
    end
    redirect_to budgets_path

  end

  def share
    @budget = Budget.find(params[:id])
    @user = User.find_by(email: params[:user][:email])

    unless @user
      flash[:danger] = "Couldn't share budget with #{params[:user][:email]}, user not found."
      redirect_to budgets_path
      return
    end

    if @user.id == current_user.id
      flash[:warning] = "Can't share budget with yourself!"
      redirect_to budgets_path
    end

    @budget.user<<@user
    if @budget.save
      flash[:success] = "Successfully shared budget with #{@user.email}!"
    else
      flash[:danger] = "Couldn't share budget with #{@user.email}."
    end
    redirect_to budgets_path
  end

  private
		def budget_params
			params.require(:budget).permit(:name, :payroll)
		end

    def permission?
      @budget = Budget.find(params[:id])
      @budget.user_ids.include? current_user.id
    end
      

end
