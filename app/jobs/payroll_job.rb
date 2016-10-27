class PayrollJob < ApplicationJob
  queue_as :default

  def perform(budget_id)
    # Perform job
    budget = Budget.find(budget_id)
    @transact = Transact.new(description: "PAYROLL", credit: true, budget_id: budget_id, amount: budget.payroll)
    @transact.user_id = budget.user.first
    if @transact.save
      # Try again
      PayrollJob.perform_later(budget_id)
    end
  end
end
