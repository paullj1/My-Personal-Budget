class TransactsController < ApplicationController
  before_action :owner?, only: [:show, :edit, :update, :destroy]

  def show
		@transact = Transact.find(params[:id])

    respond_to do |format|
      format.html
      format.json { render json: @transact }
    end
  end

  def new
    @transact = Transact.new
    @budgets = current_user.html_budgets

    respond_to do |format|
      format.html
      format.json { render json: {transact: @transact, budgets: @budgets } }
    end
  end

  def create
		@transact = Transact.new(transact_params)
    @transact.user_id = current_user.id
    @budgets = current_user.html_budgets
		if @transact.save
    	flash[:success] = "Successfully submitted new transaction!"

      respond_to do |format|
        format.html { redirect_to root_url }
        format.json { render json: { success: true } }
      end

		else
      @transact.user_id = nil # lets form hide the delete button

      respond_to do |format|
        format.html { render 'new' }
        format.json { render json: { success: false, transact: @transact, budgets: @budgets } }
      end
		end
  end

  def edit
		@transact = Transact.find(params[:id])
    @budgets = current_user.html_budgets

    respond_to do |format|
      format.html
      format.json { render json: {transact: @transact, budgets: @budgets } }
    end
  end

  def update
		@transact = Transact.find(params[:id])
		if @transact.update_attributes(transact_params)
    	flash[:success] = "Successfully updated transaction!"

      respond_to do |format|
        format.html { redirect_to root_url }
        format.json { render json: { success: true } }
      end
		else

      respond_to do |format|
        format.html { render 'edit' }
        format.json { render json: { success: false, transact: @transact } }
      end

		end
  end

  def destroy
    transact = Transact.find(params[:id])
		transact.destroy
    flash[:success] = "Transaction deleted!"

    respond_to do |format|
      format.html { redirect_to root_url }
      format.json { render json: { success: true } }
    end
  end

  private
		def transact_params
			params.require(:transact).permit(:description, :credit, :amount, :budget_id)
		end

    def owner?
      owners = Transact.find(params[:id]).budget.user_ids
      owners.include? current_user.id
    end

end
