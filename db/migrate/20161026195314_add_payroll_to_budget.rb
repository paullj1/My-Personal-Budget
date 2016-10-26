class AddPayrollToBudget < ActiveRecord::Migration[5.0]
  def change
    add_column :budgets, :payroll, :float, :default => 0
  end
end
