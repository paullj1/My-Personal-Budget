class CreateTransactions < ActiveRecord::Migration[5.0]
  def change
    create_table :transactions do |t|
      t.string :description
      t.references :budget, foreign_key: true
      t.references :user, foreign_key: true
      t.boolean :credit
      t.float :amount

      t.timestamps
    end
  end
end
