Rails.application.routes.draw do
  # For details on the DSL available within this file, see http://guides.rubyonrails.org/routing.html
  root to: "home#index"
  get '/profile' => 'users#show', as: :profile
  get '/edit_profile' => 'users#edit', as: :edit_profile

  post '/users/:id/edit_profile' => 'users#update'

  resources :budgets
  devise_for :users, controllers: { sessions: 'users/sessions' }
end
