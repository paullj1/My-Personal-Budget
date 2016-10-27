class PayrollJob < ApplicationJob
  queue_as :default

  def perform(budget_id)
    # Perform job
    budget = Budget.find(budget_id)
    @transact = Transact.new(description: "PAYROLL", credit: true, budget_id: budget_id, amount: budget.payroll)
    @transact.user_id = budget.user.first

    if @transact.save
      # Update last run
      budget.payroll_run_at = DateTime.now
      budget.save
    end
  end
end
