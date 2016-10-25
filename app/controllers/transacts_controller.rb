class TransactsController < ApplicationController
  before_action :authenticate_user!
  before_action :owner?, only: [:show, :edit, :update, :destroy]

  def show
		@transact = Transact.find(params[:id])
  end

  def new
    @transact = Transact.new
    @budgets = current_user.budget_select
  end

  def create
		@transact = Transact.new(transact_params)
    @transact.user_id = current_user.id
    @budgets = current_user.budget_select
		if @transact.save
    	flash[:success] = "Successfully submitted new transaction!"
      redirect_to root_url
		else
      @transact.user_id = nil # lets form hide the delete button
			render 'new'
		end
  end

  def edit
		@transact = Transact.find(params[:id])
    @budgets = current_user.budget_select
  end

  def update
		@transact = Transact.find(params[:id])
		if @transact.update_attributes(transact_params)
    	flash[:success] = "Successfully updated transaction!"
      redirect_to root_url
		else
			render 'edit'
		end
  end

  def destroy
    transact = Transact.find(params[:id])
		transact.destroy
    flash[:success] = "Transaction deleted!"
    redirect_to root_url
  end

  private
		def transact_params
			params.require(:transact).permit(:description, :credit, :amount, :budget_id)
		end

    def owner?
      Transact.find(params[:id]).user_id == current_user.id
    end

end
