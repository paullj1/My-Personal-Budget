class CreateBudgets < ActiveRecord::Migration[5.0]
  def change
    create_table :budgets do |t|
      t.string :name
      t.integer :authorized_users, array: true, default: '{}'
      t.references :user, foreign_key: true

      t.timestamps
    end
  end
end
