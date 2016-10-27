class AddLastPayrollRunToBudget < ActiveRecord::Migration[5.0]
  def change
    add_column :budgets, :payroll_run_at, :datetime
  end
end
