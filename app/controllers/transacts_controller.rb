class TransactsController < ApplicationController
  before_action :owner?, only: [:show, :edit, :update, :destroy]

  def index
    @transacts = Transact.all()
    respond_to do |format|
      format.json { render json: @transacts, status: :ok }
    end
  end

  def show
    @transact = Transact.find(params[:id])

    respond_to do |format|
      format.html
      format.json { render json: @transact, status: :ok }
    end
  end

  def new
    @transact = Transact.new

    @new_transact = true
    @budgets = current_user.html_budgets

    respond_to do |format|
      format.html
      format.json { render json: {transact: @transact, budgets: @budgets }, status: :ok }
    end
  end

  def create
    @t_params = transact_params
    @budgets = current_user.html_budgets

    # Transfer
    if @t_params[:credit] == 'Transfer'

      @t_params[:credit] = false
      @from_transact = Transact.new(@t_params)
      @from_transact.user_id = current_user.id

      @t_params[:credit] = true
      @to_transact = Transact.new(@t_params)
      @to_transact.budget_id = params[:to_budget_id]
      @to_transact.user_id = current_user.id

      if @to_transact.budget_id != @from_transact.budget_id and @to_transact.save and @from_transact.save
        flash[:success] = "Successfully submitted new transaction!"

        respond_to do |format|
          format.html { redirect_to root_url }
          format.json { render json: {}, status: :created }
        end

      else
        @to_transact.user_id = nil # lets form hide the delete button
        @from_transact.user_id = nil # lets form hide the delete button

        if @to_transact.budget_id == @from_transact.budget_id
          flash[:error] = "Cannot transfer to/from the same budget!"
          @transact = Transact.new(@t_params)
          @new_transact = true
        end

        respond_to do |format|
          format.html { render 'new' }
          format.json { render json: { transact: @transact, budgets: @budgets }, status: :unprocessable_entity }
        end
      end

    # Normal single transaction
    elsif @t_params[:credit] == 'Credit' or @t_params[:credit] == 'Debit'

      if @t_params[:credit] == 'Credit'
        @t_params[:credit] = true
        puts @t_params
        @transact = Transact.new(@t_params)

      else # Debit
        @t_params[:credit] = false
        @transact = Transact.new(@t_params)
      end

      @transact.user_id = current_user.id

      if @transact.save
        flash[:success] = "Successfully submitted new transaction!"

        respond_to do |format|
          format.html { redirect_to root_url }
          format.json { render json: {}, status: :created }
        end

      else
        @transact.user_id = nil # lets form hide the delete button

        respond_to do |format|
          format.html { render 'new' }
          format.json { render json: { transact: @transact, budgets: @budgets }, status: :unprocessable_entity }
        end
      end

    # Unrecognized transaction
    else
      flash[:error] = "Unrecognized transaction type: #{@t_params[:credit]}"

      respond_to do |format|
        format.html { render 'new' }
        format.json { render json: { transact: @transact, budgets: @budgets }, status: :unprocessable_entity }
      end

    end
  end

  def edit
    @transact = Transact.find(params[:id])
    @budgets = current_user.html_budgets
    @new_transact = false

    respond_to do |format|
      format.html
      format.json { render json: {transact: @transact, budgets: @budgets }, status: :ok }
    end
  end

  def update
    @transact = Transact.find(params[:id])
    @t_params = transact_params

    if @t_params[:credit] == 'Credit' or @t_params[:credit] == 'Debit'

      if @t_params[:credit] == 'Credit'
        @t_params[:credit] = true
      else
        @t_params[:credit] = false
      end

      if @transact.update(@t_params)
        flash[:success] = "Successfully updated transaction!"

        respond_to do |format|
          format.html { redirect_to root_url }
          format.json { render json: {}, status: :accepted }
        end
        return
      end
    end

    flash[:error] = "Unrecognized options..."
    respond_to do |format|
      format.html { render 'edit' }
      format.json { render json: @transact, status: :unprocessable_entity }
    end

  end

  def destroy
    transact = Transact.find(params[:id])
    transact.destroy
    flash[:success] = "Transaction deleted!"

    respond_to do |format|
      format.html { redirect_to root_url }
      format.json { render json: {}, status: :ok }
    end
  end

  private
    def transact_params
      params.require(:transact).permit(:description, :credit, :amount, :budget_id)
    end

    def owner?
      owners = Transact.find(params[:id]).budget.user_ids
      unless owners.include? current_user.id
        flash[:danger] = "Access denied!"
        respond_to do |format|
          format.html { redirect_to root_url }
          format.json { render json: {}, status: :forbidden }
        end
      end
    end

end
