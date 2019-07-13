# Place all the behaviors and hooks related to the matching controller here.
# All this logic will automatically be available in application.js.
# You can use CoffeeScript in this file: http://coffeescript.org/

setup_hooks = ->
  if !document.getElementById('transaction-type-selector') 
    return

  $('input[type=radio]').change ->
    if this.value == 'Transfer'
      $("#transfer-to-budget").show 100
    else
      $("#transfer-to-budget").hide 100
    return

$(document).on('turbolinks:load', setup_hooks)
