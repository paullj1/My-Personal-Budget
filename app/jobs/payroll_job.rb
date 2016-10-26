class PayrollJob < ApplicationJob
  queue_as :default

  def perform(budget)
    # Perform job
		@transact = Transact.new(description: "PAYROLL", credit: true, budget_id: budget.id, amount: budget.payroll)
    @transact.user_id = budget.user.first
		unless @transact.save
      PayrollJob.perform_later(budget)
    else
      # Schedule again for first of next month
      PayrollJob.set(wait_until: (Date.today + 32.days).at_beginning_of_month).perform_later(budget)
    end
  end
end
