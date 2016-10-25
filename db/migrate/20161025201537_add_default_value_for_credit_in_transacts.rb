class AddDefaultValueForCreditInTransacts < ActiveRecord::Migration[5.0]
  def change
    change_column :transacts, :credit, :boolean, :default => false
  end
end
