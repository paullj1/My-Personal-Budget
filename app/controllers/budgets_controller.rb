class BudgetsController < ApplicationController
  before_action :permission?, only: [:show, :edit, :update, :destroy, :share, :unshare]

  def index
    @budgets = current_user.budget.order(:id)

    respond_to do |format|
      format.html
      format.json { render json: @budgets, status: :ok }
    end
  end

  def show
		@budget = Budget.find(params[:id])
    @users = @budget.user

    respond_to do |format|
      format.html
      format.json { render json: {budget: @budget, users: @users }, status: :ok }
    end
  end

  def new
    @budget = Budget.new

    respond_to do |format|
      format.html
      format.json { render json: @budget, status: :ok }
    end
  end

  def create
		@budget = Budget.new(budget_params)
    @budget.user<<current_user
		if @budget.save
      @budget.run_payroll
    	flash[:success] = "Successfully created new budget!"

      respond_to do |format|
        format.html { redirect_to budgets_path }
        format.json { render json: @budget, status: :created }
      end

		else

      respond_to do |format|
        format.html { render 'new' }
        format.json { render json: @budget, status: :unprocessable_entity }
      end

		end
  end

  def edit
		@budget = Budget.find(params[:id])

    respond_to do |format|
      format.html
      format.json { render json: @budget, status: :ok }
    end
  end

  def update
		@budget = Budget.find(params[:id])
		if @budget.update_attributes(budget_params)
    	flash[:success] = "Successfully updated budget!"

      respond_to do |format|
        format.html { redirect_to budgets_path }
        format.json { render json: @budget, status: :accepted }
      end

		else

      respond_to do |format|
        format.html { render 'edit' }
        format.json { render json: @budget, status: :unprocessable_entity }
      end

		end
  end

  def destroy
    budget = Budget.find(params[:id])
		budget.destroy
    flash[:success] = "Budget deleted!"

    respond_to do |format|
      format.html { redirect_to budgets_path }
      format.json { render json: {}, status: :ok }
    end
  end

  def unshare
    @budget = Budget.find(params[:id])
    @user = User.find(params[:user_id])

    unless @user
      flash[:danger] = "Invalid user id."

      respond_to do |format|
        format.html { redirect_to budgets_path }
        format.json { render json: {}, status: :not_found }
      end
    end

    if @user.id == current_user.id
      flash[:warning] = "Can't remove yourself!"

      respond_to do |format|
        format.html { redirect_to budgets_path }
        format.json { render json: {}, status: :method_not_allowed }
      end
    end

    @budget.user.delete(@user)
    if @budget.save
      flash[:success] = "Successfully revoked budget permissions from #{@user.email}!"
      @status = :accepted
    else
      flash[:danger] = "Couldn't revoke budget permission from #{@user.email}."
      @status = :unprocessable_entity
    end

    respond_to do |format|
      format.html { redirect_to budgets_path }
      format.json { render json: {}, status: @status }
    end

  end

  def share
    @budget = Budget.find(params[:id])
    @user = User.find_by(email: params[:user][:email])

    unless @user
      flash[:danger] = "Couldn't share budget with #{params[:user][:email]}, user not found."

      respond_to do |format|
        format.html { redirect_to budgets_path }
        format.json { render json: {}, status: :not_found }
      end
      return
    end

    if @user.id == current_user.id
      flash[:warning] = "Can't share budget with yourself!"

      respond_to do |format|
        format.html { redirect_to budgets_path }
        format.json { render json: {}, status: :method_not_allowed }
      end
      return
    end

    @budget.user<<@user
    if @budget.save
      flash[:success] = "Successfully shared budget with #{@user.email}!"
      @status = :accepted
    else
      flash[:danger] = "Couldn't share budget with #{@user.email}."
      @status = :unprocessable_entity
    end

    respond_to do |format|
      format.html { redirect_to budgets_path }
      format.json { render json: {}, status: @status }
    end
  end

  private
		def budget_params
			params.require(:budget).permit(:name, :payroll)
		end

    def permission?
      @budget = Budget.find(params[:id])
      unless @budget.user_ids.include? current_user.id
				flash[:danger] = "Access denied!"
        respond_to do |format|
          format.html { redirect_to root_url }
          format.json { render json: {}, status: :forbidden }
        end
			end
    end
      

end
