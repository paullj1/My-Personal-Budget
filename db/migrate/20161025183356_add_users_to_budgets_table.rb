class AddUsersToBudgetsTable < ActiveRecord::Migration[5.0]
  def change
    create_table :users_budgets, id: false do |t|
      t.belongs_to :user, index: true
      t.belongs_to :budget, index: true
    end
  end
end
