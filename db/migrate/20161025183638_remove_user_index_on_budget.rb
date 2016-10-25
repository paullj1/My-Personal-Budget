class RemoveUserIndexOnBudget < ActiveRecord::Migration[5.0]
  def change
    remove_reference :budgets, :user, index: true
    remove_column :budgets, :authorized_users
  end
end
